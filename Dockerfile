FROM golang:1.27-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /charon .

# The scratch directory has to exist in the final image and be owned by the
# unprivileged user. A scratch base has no shell to mkdir with, so it is built
# here and copied over. Mount a tmpfs on it to keep file bytes off the disk.
RUN mkdir -p /scratch

FROM scratch

COPY --from=build /charon /charon
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /scratch /scratch

USER 65532:65532
ENV CHARON_ADDR=:1337 \
    CHARON_SCRATCH_DIR=/scratch
EXPOSE 1337

ENTRYPOINT ["/charon"]
