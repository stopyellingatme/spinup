"""HTTP routes for the service."""

from fastapi import FastAPI
from fastapi.responses import PlainTextResponse
from pydantic import BaseModel

app = FastAPI(title="Service API")


class SumRequest(BaseModel):
    """Numbers to add."""

    values: list[float]


class SumResponse(BaseModel):
    """The sum of the numbers."""

    sum: float


@app.get("/healthz", response_class=PlainTextResponse, operation_id="healthz")
def healthz() -> str:
    """Report readiness."""
    return "ok"


@app.post("/v1/sum", response_model=SumResponse, operation_id="sum")
def sum_values(request: SumRequest) -> SumResponse:
    """Add the supplied numbers."""
    return SumResponse(sum=sum(request.values))
