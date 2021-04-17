FROM golang:1.15-alpine as build

WORKDIR /go/src/altinity-test

COPY . .
RUN cd /go/src/altinity-test
RUN go build -o altinity-test

FROM alpine:3.7

COPY --from=build /go/src/altinity-test/altinity-test /usr/local/bin/altinity-test

ENTRYPOINT ["/usr/local/bin/altinity-test"]