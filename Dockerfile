FROM golang:1.14 AS build

WORKDIR /go/src/github.com/dstream.cloud/drone-config-merge
ADD . /go/src/github.com/dstream.cloud/drone-config-merge

RUN go build -o /go/bin/drone-config-merge main.go

FROM gcr.io/distroless/base-debian10
COPY --from=build /go/bin/drone-config-merge /
ENTRYPOINT ["/drone-config-merge"]
