# syntax=docker/dockerfile:1

FROM busybox:1.37 AS prep
RUN mkdir /data && chown 65532:65532 /data

FROM golang:1.26-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/westward ./cmd/westward

FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
COPY --from=build /out/westward /westward
COPY --from=prep --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["/westward"]
CMD ["serve", "--config", "/data/config.yaml"]
