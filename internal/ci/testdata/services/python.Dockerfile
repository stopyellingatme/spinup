FROM python:3.15.2-slim-trixie@sha256:c4158543ecb725b99e57d129dfd43d761d4c43e4577b5e23326aabb223cf7f4e
COPY --from=ghcr.io/astral-sh/uv:0.13.6@sha256:af22a52f5eafef3d78e551599d405c02df61919d18028f0cea8eda93bc9986d0 /uv /bin/uv
WORKDIR /app
COPY . .
RUN uv sync --locked
RUN uv run python -m pytest
