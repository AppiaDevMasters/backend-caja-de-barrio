# 01 — Pedido inicial y decisiones

Fecha: 2026-10-16. Todo lo de este archivo esta tal como lo escribio el equipo.

## Pedido inicial (textual)

> lee el contexto que esta en la carpeta de contexto, necesito empezar a crear el backend, se necesitara Go para el uso del sdk, ve contruyendo paso a paso y haciendo commits pequennos

## Respuestas del equipo a la pregunta de encuadre

Antes de escribir codigo, el agente pregunto dos cosas: cual de los dos roles se
construia primero y como se instalaba el toolchain de Go. Las respuestas, tal como
llegaron:

> **1. ¿Que construimos primero en el backend Go de este repo?**
> — Un solo servicio Go con ambos
> (preview elegido: `cmd/backend` + `internal/ase` + `internal/opclient`; "Ventaja: una sola imagen Docker, un solo .env, una sola API para el agente. Costo: mas superficie que mantener dentro del hackathon.")

> **2. ¿Como instalo el toolchain de Go en esta maquina?**
> — Te doy la contraseña de sudo
> (preview: `sudo apt install golang-go` → Go 1.22)

## Mensajes posteriores del equipo (textual)

> ya instale git

(Era Go, no git: se verifico con `go version` → `go1.22.2`.)

> Crea el

(Mensaje interrumpido a mitad de frase. Se pregunto por el resto cuatro veces y
quedo sin responder, asi que no se ejecuto nada a partir de el. Se deja aqui sin
corregir porque este archivo guarda lo que se escribio, no lo que se quiso decir.)

## Lo que el equipo decidio mas tarde, fuera de este hilo

- El contenedor del backend (T10) se le asigno a una segunda sesion de agente, que
  trabajo en el mismo arbol con superficies de archivos disjuntas.
- El flujo de Open Payments con el SDK de Go (T7-T9) se le cedio a esa misma sesion,
  porque los tres viven en el mismo paquete y conviene un solo escritor por paquete.
