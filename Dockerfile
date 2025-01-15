FROM golang:1.21
WORKDIR /app
COPY . .
RUN go build -o chaos-burn-bot
CMD ["./chaos-burn-bot"]
