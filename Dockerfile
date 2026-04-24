FROM golang:1.25.0 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download 

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o ./casparser .

FROM alpine:3.14

WORKDIR /app

COPY --from=builder /app/casparser /usr/local/bin/casparser

EXPOSE 8080

CMD ["casparser"]
