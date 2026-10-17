# 02 — Despliegue en un VPS y dominio

Fecha: 2026-10-16 y madrugada del 17. Herramienta: Claude Code (Claude Opus 5.5), en la sesión del dueño del
agente y los MCP. Todo lo de este archivo está tal como lo escribió el equipo.

## Prompts (textuales)

> vamos a subir el backend a render, para que el back no tenga que encender tunnel y lo tengamos disponible siempre, haz un pull del back y comencemos a subirlo

Respuestas a las preguntas del agente:

> ¿Quién maneja la cuenta de Render? — Yo en el dashboard (Recomendado)
>
> ¿Qué subimos a Render? — busquemos un vps que sea gratis o barato, un stack para dejar todo lo del backend listo

> cual es el stack que vamos a utilizar para desplegar el back?

> Sí, sube los 4 commits de deploy/ a main del backend.

> cual sería el costo? porque la idea es qaue sea gratis

> hoy cobrarían? y si utilizamos un stack de render? cual sería o no se podría?

> entonces voy a crear mejor el hetzer, dime donde

> ahora si, que creo aquí?

> esto

(Con capturas de la consola de Hetzner.)

> Sí, sube el commit del arreglo de Caddy a main del backend.

> bien,  vamos a conectar el dominio cajadebarrio.lat y con eso ya para que los endpoint queden bien fijos y demás y sube lo últimos cambios

> ya los puse todos

## Resultado

- `deploy/`:
  - `compose.vps.yml` (overlay sobre el compose de Rafiki: Caddy, PostGIS y el backend);
  - `Caddyfile` (sitios `op.`, `auth.`, `backend.`, `agente.` y `admin.` con Let's Encrypt; `admin.` y la UI del
    agente con clave);
  - `instalar.sh` (Docker, ufw, secretos guardados, seed, semilla de demo y verificación);
  - `semilla-demo.sql`;
  - `README.md`.
- Rafiki anuncia `https://auth.cajadebarrio.lat` como servidor de autorización (`AUTH_SERVER_GRANT_URL`).
