# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/taskbot ./cmd/taskbot

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/taskbot /taskbot
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/taskbot"]
