# Build the web app, then the Go binary that embeds it, then a minimal image.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/trylive ./cmd/trylive

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 trylive
COPY --from=build /out/trylive /usr/local/bin/trylive
USER trylive
EXPOSE 8080
ENTRYPOINT ["trylive"]
