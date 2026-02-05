# Build Stage
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o binary cmd/server/main.go

# Run Stage
FROM alpine:latest
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /app/binary .
# Copy folder template nanti saat tahap frontend sudah jadi
# COPY --from=builder /app/web ./web 

CMD ["./binary"]