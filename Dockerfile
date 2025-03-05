FROM golang:1.23.4 AS builder

WORKDIR /build
COPY . .
RUN go build -o myapp

FROM debian:bookworm-slim

WORKDIR /
COPY --from=builder /build/myapp .

# Fly.io will set this environment variable
ENV PORT=8080
EXPOSE 8080

CMD ["./myapp"]