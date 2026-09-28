FROM golang:1.26.4-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /metrics .

FROM scratch
COPY --from=build /metrics /metrics
USER 65532:65532
EXPOSE 8090
ENTRYPOINT ["/metrics"]
