# syntax=docker/dockerfile:1

FROM golang:1.19-bullseye
RUN apt update && \
		apt install webp -y && \
		apt-get clean

ENV SKIP_DOWNLOAD true
ENV VENDOR_PATH /usr/bin
ENV GIN_MODE release
WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY *.go ./

RUN go build -o /inconnu-api
EXPOSE 8080

CMD ["/inconnu-api"]
