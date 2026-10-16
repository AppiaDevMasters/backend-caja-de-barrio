# syntax=docker/dockerfile:1
#
# Imagen del backend de Caja de Barrio (el ASE que Rafiki llama: webhooks, tasas e
# IdP, mas el cliente Open Payments). El modulo no declara dependencias, asi que
# la compilacion no necesita red ni cache de modulos.

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
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
