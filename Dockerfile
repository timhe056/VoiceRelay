FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /voicerelay ./cmd/voicerelay

FROM scratch
COPY --from=build /voicerelay /voicerelay
EXPOSE 9000/udp 8080/tcp
ENTRYPOINT ["/voicerelay"]
