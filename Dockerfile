FROM golang:1.27 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o main ./cmd/server

FROM gcr.io/distroless/static-debian12

# 3000: public API. 3001: internal listener (compose network only; never publish).
EXPOSE 3000 3001
WORKDIR /app

COPY --from=builder --chown=nonroot:nonroot /app/main .

ARG VERSION
ENV VERSION=$VERSION

USER nonroot

ENTRYPOINT ["./main"]
CMD ["serve"]
