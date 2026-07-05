FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /ifynd .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H ifynd \
    && mkdir /data && chown ifynd:ifynd /data
COPY --from=build /ifynd /usr/local/bin/ifynd
USER ifynd
ENV IFYND_DB_PATH=/data/ifynd.db
EXPOSE 8080
ENTRYPOINT ["ifynd"]
