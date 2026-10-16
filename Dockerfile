# syntax=docker/dockerfile:1
#
# Imagen del backend de Caja de Barrio (el ASE que Rafiki llama: webhooks, tasas e
# IdP, mas el cliente Open Payments). El modulo declara una dependencia (el SDK
# oficial de Open Payments), asi que la compilacion baja los modulos: go.mod y
# go.sum se copian solos primero para que esa capa quede cacheada y no se repita en
# cada cambio de codigo.

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/backend ./cmd/backend

FROM alpine:3.20
# El proceso corre sin privilegios con el uid 1000, el mismo dueno de ./keys en el
# host, para que la clave del cliente Open Payments montada sea legible.
RUN adduser -D -u 1000 -h /home/backend backend
USER backend
WORKDIR /home/backend
COPY --from=build /out/backend /usr/local/bin/backend
EXPOSE 8000
ENTRYPOINT ["backend"]
