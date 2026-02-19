FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/sticky-backend \
    .


FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/sticky-backend /usr/local/bin/sticky-backend

ENV BACKEND_PORT=8080
ENV OVERSEER_URL=ws://overseer:8080/ws
ENV CONF_DIR=/data/conf

EXPOSE 8080

ENTRYPOINT ["sticky-backend"]
