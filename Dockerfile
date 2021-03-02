FROM golang:1.16

COPY . /go/src/app
WORKDIR /go/src/app

RUN go build .
CMD ["./falconstream"]