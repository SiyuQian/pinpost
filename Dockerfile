# One image: the Go server plus the admin console and demo page it serves.

FROM node:22-alpine AS admin
WORKDIR /src/web/admin
COPY web/admin/package.json web/admin/package-lock.json ./
RUN npm ci
COPY web/admin/ ./
RUN npm run build

FROM golang:1.25-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pinpost ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
# The server reads web/admin/dist and web/demo relative to its working directory.
WORKDIR /app
COPY --from=server /pinpost /app/pinpost
COPY --from=admin /src/web/admin/dist /app/web/admin/dist
COPY web/demo /app/web/demo
ENV ADDR=:8787
EXPOSE 8787
ENTRYPOINT ["/app/pinpost"]
