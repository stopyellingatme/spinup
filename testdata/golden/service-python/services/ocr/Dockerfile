# syntax=docker/dockerfile:1
FROM python:3.15.2-slim-trixie@sha256:c4158543ecb725b99e57d129dfd43d761d4c43e4577b5e23326aabb223cf7f4e
COPY --from=ghcr.io/astral-sh/uv:0.13.6@sha256:af22a52f5eafef3d78e551599d405c02df61919d18028f0cea8eda93bc9986d0 /uv /bin/uv
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy PYTHONDONTWRITEBYTECODE=1
COPY pyproject.toml uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv uv sync --locked --no-dev
COPY service ./service
RUN groupadd --gid 10001 service && useradd --uid 10001 --gid service --no-create-home service
USER service
ENV PORT=8080
EXPOSE 8080
CMD ["/app/.venv/bin/uvicorn", "service.main:app", "--host", "0.0.0.0", "--port", "8080"]
