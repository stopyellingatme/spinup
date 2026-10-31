FROM node:24-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@12.11.2 --activate
COPY . .
RUN pnpm install --frozen-lockfile
RUN pnpm test
RUN if node -e 'process.exit(Object.hasOwn(require("./package.json").scripts ?? {}, "typecheck") ? 0 : 1)'; then pnpm run typecheck; fi
