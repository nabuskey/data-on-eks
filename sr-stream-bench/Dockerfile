# Stage 1: build the static binary.
# The base image is pinned to a digest. So the build is repeatable.
FROM golang:1.25@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80 AS build
WORKDIR /src

# Copy the module files first. This caches the dependency download.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source and build. CGO is off, so the binary is static.
# kafka-go is pure Go, so it needs no C library.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kafka-bench .

# Stage 2: put the binary in a small, non-root image.
# The base image is pinned to a digest. So the image is repeatable.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/kafka-bench /kafka-bench
ENTRYPOINT ["/kafka-bench"]
