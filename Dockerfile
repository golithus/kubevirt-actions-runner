FROM golang:1.23.4-alpine3.20 AS build

WORKDIR /go

COPY . .

RUN go build -o /kar cmd/kar/main.go

FROM scratch

COPY --from=build /kar /opt/kar

USER 10001:10001

ENTRYPOINT [ "/opt/kar"]