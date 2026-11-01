"""HTTP routes of the PDF service.

The parent process never imports WeasyPrint. Each render runs in a child
process (service.render_job), so a timeout can stop it and RLIMIT_AS bounds
its memory.
"""

import asyncio
import json
import logging
import sys
import time
from collections.abc import AsyncIterator, Sequence
from contextlib import asynccontextmanager, suppress
from typing import Any

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, PlainTextResponse, Response
from opentelemetry import trace

from service import limits, telemetry

log = logging.getLogger("service.render")
log.setLevel(logging.INFO)
if not logging.getLogger().handlers:
    logging.basicConfig(format="%(asctime)s %(levelname)s %(name)s %(message)s")

CHILD_COMMAND = (sys.executable, "-m", "service.render_job")


class RenderError(Exception):
    """A render failed with an HTTP status."""

    def __init__(self, status: int, title: str, detail: str = "") -> None:
        super().__init__(title)
        self.status = status
        self.title = title
        self.detail = detail


def problem(status: int, title: str, detail: str = "", headers: dict[str, str] | None = None) -> JSONResponse:
    """Return an application/problem+json answer."""
    body: dict[str, Any] = {"type": "about:blank", "title": title, "status": status}
    if detail:
        body["detail"] = detail
    return JSONResponse(body, status_code=status, media_type="application/problem+json", headers=headers)


class Renderer:
    """Run render children with a concurrency limit and a bounded queue."""

    def __init__(
        self,
        settings: limits.Limits,
        command: Sequence[str] = CHILD_COMMAND,
        tracer_provider: trace.TracerProvider | None = None,
    ) -> None:
        self.settings = settings
        self.command = tuple(command)
        self.tracer = trace.get_tracer("service.render", tracer_provider=tracer_provider)
        self.pending = 0
        self._slots: dict[asyncio.AbstractEventLoop, asyncio.Semaphore] = {}

    def admit(self) -> bool:
        """Take a place in the render queue. Return False when the queue is full."""
        if self.pending >= self.settings.concurrency + self.settings.queue_max:
            return False
        self.pending += 1
        return True

    def release(self) -> None:
        """Give back a place that admit took."""
        self.pending -= 1

    async def render(self, template: str, payload: bytes, deadline: float) -> bytes:
        """Wait for a slot, run one child, and return the PDF."""
        loop = asyncio.get_running_loop()
        slots = self._slots.get(loop)
        if slots is None:
            # A semaphore belongs to one event loop. Tests start several loops.
            self._slots = {loop: asyncio.Semaphore(self.settings.concurrency)}
            slots = self._slots[loop]
        try:
            async with asyncio.timeout_at(deadline):
                async with slots:
                    started = time.monotonic()
                    with self.tracer.start_as_current_span("pdf.render") as span:
                        span.set_attribute("pdf.template", template)
                        pdf = await self._run(payload)
                        span.set_attribute("pdf.bytes", len(pdf))
                        span.set_attribute("pdf.render_ms", int((time.monotonic() - started) * 1000))
                        return pdf
        except TimeoutError:
            raise RenderError(504, "The render took too long") from None

    async def _run(self, payload: bytes) -> bytes:
        child = await asyncio.create_subprocess_exec(
            *self.command,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=limits.APP_DIR,
        )
        stdout = asyncio.create_task(_read_capped(child.stdout, limits.MAX_OUTPUT_BYTES))
        stderr = asyncio.create_task(_drain_capped(child.stderr, limits.MAX_STDERR_BYTES))
        try:
            with suppress(BrokenPipeError, ConnectionResetError):
                child.stdin.write(payload)
                await child.stdin.drain()
                child.stdin.close()
            output, overflow = await stdout
            if overflow:
                child.kill()
                raise RenderError(500, "The PDF is larger than the limit")
            code = await child.wait()
            error_text = (await stderr).decode("utf-8", "replace").strip()
        finally:
            if child.returncode is None:
                child.kill()
                await child.wait()
            for task in (stdout, stderr):
                task.cancel()
        if code == limits.EXIT_OK and output.startswith(b"%PDF-"):
            return output
        if code == limits.EXIT_NOT_FOUND:
            raise RenderError(404, "Unknown template")
        if code == limits.EXIT_DATA:
            raise RenderError(422, "The template needs a key that data does not have", error_text)
        if code == limits.EXIT_FETCH:
            raise RenderError(500, "The template loads a URL that is not allowed")
        log.warning("render child failed code=%s stderr=%s", code, error_text)
        raise RenderError(500, "The render failed")


async def _read_capped(stream: asyncio.StreamReader, cap: int) -> tuple[bytes, bool]:
    """Read to the end, or stop at once when the size passes cap."""
    chunks: list[bytes] = []
    size = 0
    while chunk := await stream.read(64 * 1024):
        size += len(chunk)
        if size > cap:
            return b"", True
        chunks.append(chunk)
    return b"".join(chunks), False


async def _drain_capped(stream: asyncio.StreamReader, cap: int) -> bytes:
    """Keep the first cap bytes and discard the rest."""
    kept = bytearray()
    while chunk := await stream.read(64 * 1024):
        if len(kept) < cap:
            kept += chunk[: cap - len(kept)]
    return bytes(kept)


async def read_body(request: Request, limit: int) -> bytes:
    """Read the request body. Raise RenderError 413 above limit."""
    declared = request.headers.get("content-length")
    if declared is not None and declared.isdigit() and int(declared) > limit:
        raise RenderError(413, "The request body is too large")
    body = bytearray()
    async for chunk in request.stream():
        body += chunk
        if len(body) > limit:
            raise RenderError(413, "The request body is too large")
    return bytes(body)


def parse_request(body: bytes) -> tuple[str, dict[str, Any]]:
    """Check the JSON request. Raise RenderError 422 or 404."""
    try:
        request = json.loads(body)
    except ValueError:
        raise RenderError(422, "The body is not JSON") from None
    if not isinstance(request, dict) or set(request) != {"template", "data"}:
        raise RenderError(422, "The body must have exactly the keys template and data")
    template, data = request["template"], request["data"]
    if not isinstance(data, dict):
        raise RenderError(422, "data must be an object")
    if not isinstance(template, str) or limits.template_path(template) is None:
        raise RenderError(422, "template must match ^[a-z][a-z0-9_]{0,63}$")
    if not limits.template_path(template).is_file():
        raise RenderError(404, "Unknown template")
    return template, data


def create_app(
    settings: limits.Limits | None = None,
    command: Sequence[str] = CHILD_COMMAND,
    tracer_provider: trace.TracerProvider | None = None,
) -> FastAPI:
    """Build the application. Tests pass small limits or a fake child command."""
    settings = settings or limits.Limits.from_env()
    renderer = Renderer(settings, command, tracer_provider)

    @asynccontextmanager
    async def lifespan(_: FastAPI) -> AsyncIterator[None]:
        """Flush traces before the service stops."""
        yield
        if provider is not None:
            provider.shutdown()

    app = FastAPI(title="PDF Service API", lifespan=lifespan)
    app.state.renderer = renderer
    provider = telemetry.setup(app)

    @app.get("/healthz", response_class=PlainTextResponse, operation_id="healthz")
    def healthz() -> str:
        """Report readiness."""
        return "ok"

    @app.post("/v1/render", operation_id="render", response_class=Response)
    async def render(request: Request) -> Response:
        """Render a template to PDF."""
        started = time.monotonic()
        template = "-"
        if not renderer.admit():
            log.info("render template=%s status=%d ms=%d", template, 503, 0)
            return problem(503, "The render queue is full", headers={"Retry-After": str(limits.RETRY_AFTER_SECONDS)})
        try:
            deadline = asyncio.get_running_loop().time() + settings.timeout_seconds
            body = await read_body(request, settings.max_body_bytes)
            template, data = parse_request(body)
            payload = json.dumps({"template": template, "data": data}).encode()
            del body, data
            pdf = await renderer.render(template, payload, deadline)
        except RenderError as error:
            log.info("render template=%s status=%d ms=%d", template, error.status, _ms(started))
            return problem(error.status, error.title, error.detail)
        finally:
            renderer.release()
        log.info("render template=%s status=%d ms=%d", template, 200, _ms(started))
        return Response(pdf, media_type="application/pdf")

    return app


def _ms(started: float) -> int:
    return int((time.monotonic() - started) * 1000)


app = create_app()
