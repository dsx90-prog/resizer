FROM golang:1.21-alpine AS builder

WORKDIR /app
RUN apk update && apk add build-base

COPY . .

RUN go build -ldflags="-w -s" -o resizer main.go

FROM alpine

RUN apk update && apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /app/resizer ./resizer

RUN chmod +x ./resizer

EXPOSE 8085

CMD ["./resizer", "server"]