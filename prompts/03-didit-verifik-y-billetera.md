# 03 — Didit, Verifik y la billetera virtual

Fecha: madrugada del 2026-10-17. Herramienta: Claude Code (Claude Opus 5.5). Textual.

## Prompts (textuales)

> la prueba que vamos a hacer es a registrar a una persona cualquiera utilizando un token de open payment, ademas, hacer todo el proceso de punta a punta desde la ui de adk, entonces ahí es donde veremos que pasa, una persona abre la ui y otra tambien, por ejemplo yo sería el tendero, y mi compañero sería el que va a transferir, dime que falta?
>
> la prueba la haremos desde la ui de adk

> la prueba consiste en:
>
> 1. registramos a un tendero desde el panel
> 2. registramos a una persona que es la que enviará la plata
> 3. hacemos todo el proceso de seguridad para ambos con didit
> 4. se hace verificacíon con verifik una vez didit ya bajó los datos del usuario
> 5. habrá como una wallet virtual que le mostrará al usuario cuanto tiene y al tendero tambien
>
> esa debe ser la prueba

> hazme el de ejemplo para ponerlos ahí.
>
> y tambien haz el .env para poner las llaves necesarias.

> donde está este archivo?

> para didit, voy a poner estas:
>
> didit_workflow_id
> didit_biometric_workflow_id
> didit_api_key
>
> con esas 3 podemos hacer la prueba y verifik esta
>
> verifik_api_key

> cual sería el workflowid?

> ya puse:
>
> ```
> DIDIT_API_KEY=
> DIDIT_WORKFLOW_ID=
> DIDIT_BIOMETRIC_WORKFLOW_ID=
> VERIFIK_API_KEY=
> ```
>
> ya los puse es necesario el webhook? ya que estoy utilizando la cuenta de otro proyecto en sandbox, es necesario el webhook o con esas mismas puedo crear otro wehbook?

(Los valores se pusieron en `deploy/llaves.env` del servidor, nunca en el chat ni en git.)

> DIDIT_WEBHOOK_SECRET ya lo puse

> hagamos esta primera prueba sin webhook, luego ya hacemos un pplan de implementación

> ahora falta las llaves de open payments y quedamos listos?

> haz un pull del back e implementalo y sube todo lo necesario al vps y dejemos eso listo

> Sí, hagamos la prueba completa desde la UI de ADK: registro, Didit con LISTO y ver la billetera.

## Resultado

- **C8 (contrato 5.5 a 5.7):**
  - verificaciones de Didit sin depender del webhook: el estado se consulta a la API de Didit y se aplica una sola vez;
  - alta (documento y prueba de vida) y retiro (comparación facial);
  - Verifik con el número que Didit leyó; si no se confirma, `revision_manual`;
  - billetera de Open Payments creada en el Rafiki propio al aprobar el registro;
  - el webhook ignora las sesiones de otro proyecto.
- **Billetera virtual:** tabla `movimientos`, `GET /pagos/saldo` y carga inicial de `SALDO_INICIAL_DEMO_COP`.
- **`deploy/llaves.env.example`:** Didit, Verifik y el callback a `/didit/retorno`.
