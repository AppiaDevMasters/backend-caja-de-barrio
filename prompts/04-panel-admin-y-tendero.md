# 04 — `/admin` para el panel y alta del tendero

Fecha: madrugada del 2026-10-17. Herramienta: Claude Code (Claude Opus 5.5). Textual.

## Prompts (textuales)

> bien, ahora el panel panel acepta? cual es la base url para el front conecte todo? revisa ese repo y haz un .md v3 de lo que falte para ya terminar la prueba

(Con una captura de la UI de ADK: el registro quedó en revisión manual.)

> Sí, implementa en el backend 3.1 y 3.2 del v3 (CORS, login, personas con aprobar/rechazar) y despliégalo.

> dime donde creaste las billeteras si el private.key está aquí "C:\Users\Usuario\Downloads\private.key"

> Sí, aprueba mi registro desde /admin con un motivo y revisa que me llegue el aviso y el saldo en la UI de ADK.

> y dime donde veo la wallet

> esto donde lo veo?

> para entrar en la ui de refiki donde es?

> perfecto, ya me sale 0, entonces como alguien mas abre una cuenta? para que me envie que soy el tendero

> el no será tendero, el será un usuario particular

> si, entonces el tiene que poner tambien las credenciales de admin verdad?

> ya se registró cristian, revisa, porque el será el particular que va a enviar el dinero

> crea mi tienda para comenzar a hacer la prueba end to end

> como estamos en pruebas, has que mi tienda ya se valide con lo que me falta solo lo facial para continuar la pruebas

## Resultado

- **`/admin` (`internal/admin`):**
  - CORS solo para `PANEL_ORIGINS`;
  - sesión en cookie firmada (`HttpOnly`, `Secure`, `SameSite=None`) con usuarios por rol;
  - claves generadas por `instalar.sh` en `llaves.env`;
  - auditoría con motivo obligatorio.
- **Personas:** lista, ficha, aprobar (crea la billetera y la carga inicial, y avisa por el agente) y rechazar.
- **Tenderos:**
  - `POST /admin/tenderos` crea la tienda en `pendiente_verificacion` con su wallet;
  - tiendas: lista, ficha, activar y suspender.
- **Alta del tendero (`alta_tendero`):** Didit y Verifik; la tienda queda `activa` o en `revision_manual`. La 5.1
  devuelve `estado_tienda`.
- **En la prueba**, la tienda se activó a mano por `/admin`, con el motivo en la auditoría.
