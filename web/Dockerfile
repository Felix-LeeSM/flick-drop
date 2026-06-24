# syntax=docker/dockerfile:1

ARG NODE_IMAGE=node:24.15.0-alpine
ARG WEB_RUNTIME_IMAGE=nginx:1.29-alpine

FROM ${NODE_IMAGE} AS build
WORKDIR /app

ENV CI=true
RUN corepack enable && corepack prepare pnpm@10.20.0 --activate

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
RUN pnpm install --frozen-lockfile

COPY . .

ARG PUBLIC_FLICK_API_BASE_URL=/
ARG PUBLIC_FLICK_LOCAL_FILE_MAX_BYTES=1048560
ARG PUBLIC_FLICK_MIN_TTL_SECONDS=300
ARG PUBLIC_FLICK_MAX_TTL_SECONDS=604800
ARG PUBLIC_FLICK_DEFAULT_TTL_SECONDS=3600
ENV PUBLIC_FLICK_API_BASE_URL=${PUBLIC_FLICK_API_BASE_URL} \
	PUBLIC_FLICK_LOCAL_FILE_MAX_BYTES=${PUBLIC_FLICK_LOCAL_FILE_MAX_BYTES} \
	PUBLIC_FLICK_MIN_TTL_SECONDS=${PUBLIC_FLICK_MIN_TTL_SECONDS} \
	PUBLIC_FLICK_MAX_TTL_SECONDS=${PUBLIC_FLICK_MAX_TTL_SECONDS} \
	PUBLIC_FLICK_DEFAULT_TTL_SECONDS=${PUBLIC_FLICK_DEFAULT_TTL_SECONDS}

RUN pnpm build

FROM ${WEB_RUNTIME_IMAGE} AS runtime

COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=build /app/build /usr/share/nginx/html

RUN chown -R nginx:nginx /usr/share/nginx/html /var/cache/nginx /var/run /tmp

USER nginx
EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
