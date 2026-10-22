# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# `docker compose --profile test run --rm web-test` runs typecheck + component tests.
FROM build AS test
CMD ["sh", "-c", "npm run typecheck && npm test"]

# `docker compose --profile e2e run --rm e2e` drives the running stack in Chromium.
FROM mcr.microsoft.com/playwright:v1.56.0-noble AS e2e
WORKDIR /e2e
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY playwright.config.ts tsconfig.json ./
COPY e2e ./e2e
CMD ["npx", "playwright", "test"]

FROM nginx:1.27-alpine AS runtime
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
