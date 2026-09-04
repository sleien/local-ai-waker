FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ai-waker .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=build /out/ai-waker /usr/local/bin/ai-waker
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/ai-waker"]
