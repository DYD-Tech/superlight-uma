# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/superlight-uma ./cmd/superlight-uma \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/superlight-uma /superlight-uma
COPY --from=build --chown=65532:65532 /out/data /data
ENV UMA_ADDR=:8080 \
    UMA_DB_PATH=/data/uma.db
VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/superlight-uma"]
