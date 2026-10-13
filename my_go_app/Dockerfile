FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY . .
RUN go mod tidy && go build -o main .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/main .
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/static ./static
EXPOSE 8080
CMD ["./main"]
