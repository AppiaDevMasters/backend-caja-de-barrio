# Proveedores de identidad del backend Go: Verifik (C7) y Didit (cliente de C8)

Los dos clientes de proveedor que consumen las rutas de identidad del contrato: la
validacion de cedula contra Verifik y la verificacion de identidad de Didit con la
verificación de la firma de su webhook. Superficie propia y exclusiva:
`internal/verifik/**` y `internal/didit/**`, acordada en el `L6` de
[`contrato-interno-y-pagos.md`](contrato-interno-y-pagos.md). Las rutas
(`POST /cedulas/verificar`, `POST /verificaciones`, `GET /verificaciones/{id}`,
`POST /webhooks/didit`) y el envoltorio de errores son de la sesion 01a1466b, que
envuelve estos clientes.

Documento de la sesion 01a14664. El par no lo edita y esta sesion no edita ninguno
de los dos documentos del par.

## Specs

Las citas marcadas como textuales de contrato provienen de
`contexto-caja-de-barrio/contrato-backend.md`, seccion 5, leido con permiso explicito
del humano (solo lectura, fuera de la raiz del proyecto).

S1. **Validacion de cedula con Verifik (contrato 5.3).** Textual de contrato:

> ```json
> // request
> { "tipo": "CC", "numero": "1036123456" }
> // 200
> { "valida": true, "nombre_completo": "LAURA GOMEZ" }
> ```
> Verifik: `GET https://api.verifik.co/v2/co/cedula?documentType=CC&documentNumber=...` con `Authorization: Bearer <VERIFIK_TOKEN>`.

S2. **Creacion de la sesion de Didit (contrato 5.5).** Textual de contrato:

> ```json
> // request
> { "persona_id": "per_laura", "motivo": "retiro" }   // motivo: "alta" | "retiro"
> // 201
> { "session_id": "did_0647d5ea", "url_verificacion": "https://verify.didit.me/session/..." }
> ```
> - Didit: `POST https://verification.didit.me/v3/session/` con `x-api-key: DIDIT_API_KEY`, `workflow_id`,
>   `vendor_data` = nuestro `session_id`, y `callback` = `https://<túnel>/webhook/didit`.

S3. **Lectura de la decision (contrato 5.6).** Textual de contrato:

> ```json
> { "aprobada": null, "score": null, "pendiente": true }
> ```

S4. **Webhook de Didit y su firma (contrato 5.7).** Textual de contrato:

> El gateway del agente recibe el webhook de Didit y lo reenvía **sin tocarlo**:
>
> ```json
> // request
> { "cuerpo": "<body crudo del webhook, como string>", "firma": "<header X-Signature-V2>" }
> // 200 si es un resultado final
> { "procesado": true, "session_id": "did_0647d5ea", "aprobado": false, "persona_id": "per_laura",
>   "whatsapp": "573000000001", "motivo": "retiro" }
> // 200 si es un estado intermedio
> { "procesado": false, "ignorado": "In Progress" }
> ```
> - Valida la firma con `DIDIT_WEBHOOK_SECRET`. Si no es válida → `401 FIRMA_INVALIDA`.

S5. **Superficie y acoplamiento, textual del `L6` del par** (la regla que hace que las
dos sesiones no se pisen en el mismo arbol): "Solo biblioteca estandar (`net/http`,
`encoding/json`, `crypto/hmac`, `crypto/sha256`). Asi no tocas `go.mod` ni `go.sum`.",
"Los secretos entran por parametro en el constructor (`verifik.New(token string)`,
`didit.New(apiKey, workflowID, webhookSecret string)`). No leas `os.Getenv` y no
toques `internal/config` ni `env.example`.", "No registres rutas HTTP ni toques
`internal/api`, `cmd/backend` ni `internal/httpserver`. Las rutas `POST /verificaciones`
y `POST /webhooks/didit` las escribo yo envolviendo tu cliente." y "Pruebas en tus
propios paquetes contra `httptest`."

S6. **Invariante de datos personales (contrato 2, textual).** "No se guardan selfies ni
fotos de documentos: solo la referencia de la sesión de Didit." y "Las cédulas se
muestran enmascaradas." Aplica al cliente en un punto concreto: ningun mensaje de
error ni de log puede llevar el numero de documento completo.

S7. **La forma canonica que firma `X-Signature-V2`.** No esta en el contrato: sale de
la documentacion del proveedor (el encargo de integracion de webhooks de Didit). El
secreto del webhook firma **el cuerpo canonico**, no los bytes crudos, y la forma
documentada es `json.dumps(body, sort_keys=True, separators=(",", ":"),
ensure_ascii=False)` despues de aplicar su `shorten_floats`, que serializa como entero
todo float con valor entero. El encabezado `X-Timestamp` trae segundos Unix y la
ventana de frescura documentada es de 300 segundos. Esta spec existe porque Go y
Python no coinciden en tres detalles que producen un 401 silencioso sobre una entrega
valida: ver L5.

S8. **Variables de entorno (contrato 10, textual).** `VERIFIK_TOKEN`;
`DIDIT_API_KEY`, `DIDIT_WORKFLOW_ID`, `DIDIT_WEBHOOK_SECRET`. Confirmadas por el `L11`
del par. Este documento las usa como **parametros de constructor**, no las lee del
entorno; quien las lee es la capa de configuracion del par, y quien las agrega a
`env.example`, `docs/configuracion.md` y `README.md` es su tarea C17.

## Tasks

| ID | Specs | Tarea | Superficie | Commit |
| --- | --- | --- | --- | --- |
| V1 | S1,S5,S6,S8 | `internal/verifik`: `New(token)`, `VerifyDocument`, 404 como resultado de negocio, validacion local del documento, sin eco del numero | `internal/verifik` | `306f4fb` |
| D1 | S2,S5,S7,S8 | `internal/didit`: `New(apiKey, workflowID, webhookSecret)`, `CreateSession` (201, campo `url`), `Decision` (pendiente sin veredicto, score) | `internal/didit` | `ffa93fa` |
| D2 | S4,S7 | `internal/didit`: canonicalizacion, `VerifyWebhook` con comparacion de tiempo constante y ventana de frescura, `ParseWebhook` con `Approved`/`Final` insensibles a mayusculas | `internal/didit` | `ffa93fa` |
| D3 | S7 | Paridad con la forma documentada, medida contra Python: notacion de floats, enteros exactos, el literal `-0`, escapes de control | `internal/didit` | `ffa93fa` |
| V2 | — | Verificacion independiente y correccion de los defectos confirmados por medicion | ambos | `306f4fb`, `ffa93fa` |

La rebanada esta cerrada, verde y commiteada. Las tres tareas de codigo cierran con
commit de unidad de trabajo en `main`, con las pruebas junto al comportamiento, y este
documento cierra con el suyo.

## Log

L1 (pedido del humano, textual, con su ortografia): "si, ten encuenta que otro agente
esta trabajdno para que no se pisen y haya conflictos en los cambios". Llego como
respuesta a la propuesta de arrancar T7 (`internal/opclient`), asi que autorizaba ese
trabajo con la condicion de coordinacion.

L2 (2026-10-16, fallo del humano sobre la titularidad, que es lo que define esta
rebanada). Antes de escribir nada se midio el repositorio y se encontro que la sesion
peer `01a1466b` habia reclamado **T7-T9 por escrito** en el `L24` del documento del ASE
(que supersede el punto 1 de su `L23`), ampliando su alcance a dos incoming payments y
dos quotes. Como un paquete admite un solo escritor, se le pidio al humano el fallo
sobre la titularidad. El humano eligio, textual de la opcion seleccionada: **"El par
hace T7-T9; yo tomo Verifik + Didit"**. El reparto quedo escrito por las dos partes en
el `L6` del documento del par, con lista de exclusion mutua, y despues enmendado en su
`L11`.

L3 (2026-10-16, lectura del contrato y de las dos APIs de proveedor; medicion, no
memoria). El contrato se leyo completo con el permiso de lectura que el humano concedio
para esa ruta. Las APIs se verificaron contra su documentacion publica, y de ahi
salieron cuatro hechos que ninguna suposicion habria acertado:

1. **Crear sesion responde 201 Created, no 200.** El cliente acepta solo 201.
2. **El campo de la URL hospedada se llama `url`, no `verification_url`**; la propia
   documentacion del proveedor lo aclara porque es una confusion frecuente.
3. **La documentacion de Didit se contradice sobre el estado.** La prosa dice que los
   estados son *case-sensitive* y en formato titulo (`Approved`, `In Review`,
   `Kyc Expired`), y sus ejemplos JSON devuelven `"APPROVED"` en mayusculas. Se compara
   sin distinguir mayusculas y se conserva el texto tal cual llego, para no romperse
   con la variante que el sandbox mande.
4. **El endpoint de decision es `GET /v3/session/{id}/decision/`** y sus bloques de
   features son **arrays** (`face_matches[0].score`), porque un workflow puede correr la
   misma feature mas de una vez.

De Verifik: el nombre sale de `data.fullName`; solo acepta `CC` y `PPT`; el numero va
sin espacios ni puntos y de 5 a 10 caracteres; y un 404 significa que la
Registraduria no tiene ese documento, o sea **un resultado de negocio y no una falla
del proveedor**.

Decision registrada de esta sesion: el contrato 5.6 pide **un** `score` y el proveedor
devuelve uno por feature. Se toma el de face match, con el de liveness como respaldo, y
se informo al par para que no lo bifurque en su ruta.

L4 (2026-10-16, construccion con prueba primero, y el RED observado). Las dos
rebanadas se escribieron prueba primero y el fallo se observo antes de implementar:
`internal/didit` arranco con 41 fallos de asercion y `internal/verifik` con 5 pruebas
en rojo. En verifik la prueba se escribio antes que el tipo, asi que hubo una ventana
de un minuto con un fallo de compilacion (`didit_test.go:118:19: undefined: Error`) que
la sesion peer alcanzo a medir y que despues repitio como si fuera el estado vigente;
su `L11` punto 4 lo corrige solo, medido. Leccion para las dos: un dato medido tambien
caduca, y mientras las dos sesiones escriban en el mismo arbol `go test ./...` no es una
senal limpia para ninguna.

Correccion de convencion: los identificadores se escribieron primero en espanol
(`Cedula`, `Valida`, `NombreCompleto`) y se renombraron a ingles (`Document`, `Valid`,
`FullName`) al notar que la spec S10 del documento del ASE exige "Codigo e
identificadores en ingles". Se hizo antes de publicar la API al par.

L5 (2026-10-16, verificacion independiente y **tres defectos reales**, medidos antes de
aceptarlos). La verificacion independiente (`gentle-ai-verify`, read-only) dio R1, R2,
R5 y R6 verificados y R3/R4 parciales, y reporto diez hallazgos. **Ninguno se acepto sin
reproducirlo**, y esa decision encontro algo importante: la forma canonica de la spec S7
no es la que Go produce por defecto, y tres divergencias rompian la verificacion de
firmas.

1. **Notacion de floats.** Python escribe un float en forma posicional mientras su
   exponente decimal este en `[-4, 16)`, y en forma exponencial fuera de esa ventana; Go
   con `%g` cambia a exponencial en 6. Medido: `json.dumps(1234567.5)` da `1234567.5` y
   `strconv.FormatFloat(1234567.5,'g',-1,64)` daba `1.2345675e+06`. Un cuerpo con ese
   valor se habria recepcionado como no firmado: **401 silencioso sobre una entrega
   valida**.
2. **Enteros exactos.** El `shorten_floats` del proveedor convierte un float entero a
   `int`, y el `int` de Python es exacto: medido, `int(1e30)` es
   `1000000000000000019884624838656`, el valor binario, no el decimal redondo. Corregido
   con `math/big`.
3. **El literal `-0`.** Medido: `json.loads("-0")` es `0` y `json.dumps` escribe `0`.
   Un entero se conserva verbatim (para no redondear identificadores mayores a 2^53)
   salvo ese caso.

Los tres fallaban **cerrado**: rompian el flujo de identidad, nunca dejaban pasar un
cuerpo no firmado. Los valores esperados de las pruebas de paridad salieron de ejecutar
el canonicalizador de Python, no de razonar sobre el.

**Y dos hallazgos se rechazaron tras medir.** El verificador afirmo que Go emite
`\u0008`/`\u000c` donde Python emite `\b`/`\f`; es falso: `encoding/json` tiene esos dos
casos cortos y coinciden, y las pruebas de backspace y form feed ya pasaban antes de
tocar nada. Y la expectativa propia para `0x7f` estaba mal: con `ensure_ascii=False`
Python lo deja **literal**, igual que Go, asi que el defecto era la prueba y no el
codigo; lo detecto la prueba al correr. Registrado porque importa: un verificador
independiente aporta mucho y tambien inventa.

Otro hallazgo si se corrigio: **F6**, el mensaje de error de `verifik` repetia el numero
de documento completo, contra las dos invariantes de la spec S6 (cedulas enmascaradas y
no guardar datos del documento). Ahora no lo lleva, con prueba que lo fija.

L6 (2026-10-16, los dos limites que **no** son corregibles dentro de esta superficie y
por eso se documentan y se le pasan a quien consume el paquete):

1. **La frescura no autentica nada.** `X-Signature-V2` firma **solo el cuerpo**; el
   `X-Timestamp` queda fuera del digest. Medido sobre la forma documentada: un par
   `(cuerpo, firma)` capturado se puede repetir indefinidamente reescribiendo la
   cabecera del timestamp a "ahora". La ventana de ±300 s es un filtro contra ruido, no
   proteccion contra repeticion. La defensa real es de la ruta que consume el cliente:
   **deduplicar por `event_id` antes de aplicar efectos**, o sea antes de marcar a la
   persona como verificada y de escribir los 30 minutos de vigencia. Queda como
   advertencia en el comentario de `VerifyWebhook` y se le envio por mensaje al par.
2. **Un `callback` vacio no falla, desvia.** El proveedor trata `callback` como
   opcional y cae al valor por defecto del workflow, asi que omitirlo manda a la persona
   a un destino que este backend no controla. El contrato siempre tiene
   `DIDIT_CALLBACK_URL`, asi que la ruta debe pasarlo siempre.

L7 (2026-10-16, cierre de la rebanada, medido). `internal/verifik`: 5 pruebas, cobertura
85.7%. `internal/didit`: 8 pruebas incluidas las de paridad con Python, cobertura 88.5%.
79 casos contando subtests. `go vet ./...` y `gofmt -l .` no dicen nada y
`go test ./...` pasa los diez paquetes, con `internal/opclient` del par incluido.

**Lo que no esta verificado, y se dice en vez de disimularse**: no hay `VERIFIK_TOKEN`
ni credenciales `DIDIT_*`, asi que **nunca se llamo a `api.verifik.co` ni a
`verification.didit.me`**. Todo se probo contra dobles de `httptest` construidos desde
el formato documentado: los nombres exactos de campos del endpoint de decision, el
`data.fullName` de Verifik y el esquema real de la firma se apoyan en documentacion, no
en una llamada real. La primera llamada verdadera sigue sin verificar.

L8 (2026-10-16, autorizacion del commit con su condicion, y cierre de la rebanada). El
humano autorizo textualmente: "hazlo pero revisa los cambios que esta implementado el
otro agente antes de realizar algun commit". Se reviso lo del par **antes** de
commitear, y la revision no fue un tramite: **el arbol estaba rojo**.

Lo medido: `go test ./...` fallaba en `internal/opclient` con cuatro pruebas
(`TestCreateIncomingPayments`, `TestCreateQuotes`,
`TestCreateIncomingPaymentsRejectsParamsItCannotUse`,
`TestCreateIncomingPaymentsRequiresANonInteractiveGrant`) y `gofmt -l .` nombraba
`internal/opclient/opclient_test.go`. **Lo rojo era trabajo en vuelo del par, no lo
commiteado**, y se comprobo en vez de suponerlo: ninguna de esas cuatro pruebas existe
en `HEAD` (cero ocurrencias) y su archivo de pruebas paso de 221 lineas y 5 funciones a
819 lineas y 11 funciones, o sea que agrego seis pruebas y todavia no las implemento.
Es el RED de su propio TDD, tal como el lo describio al decir que le faltaba la segunda
mitad de T7. `HEAD` esta verde y su superficie no se cruza con la de esta sesion. La
leccion vale para las dos: un arbol rojo no dice de quien es la culpa, dice donde mirar.

Se reviso ademas el cambio suyo que podia romper este commit sin tocar ningun archivo
de esta sesion: su `Dockerfile` ahora copia `go.mod` y `go.sum` antes de
`go mod download`, que es exactamente lo que hace falta despues de declarar el SDK como
dependencia. Ese riesgo no existe. El indice estaba limpio: nada preparado por error.

Los tres commits de unidad de trabajo, con rutas nombradas y una guarda que abortaba si
quedaba algo ajeno preparado:

| Commit | Contenido |
| --- | --- |
| `306f4fb` | `feat(verifik)`: V1 |
| `ffa93fa` | `feat(didit)`: D1, D2 y D3 |
| el commit de este documento | `docs(odd)`: el seguimiento de la rebanada |

No se uso `git add -A` ni `git add .` en ningun momento, como acordaron las dos sesiones
en el `L11` del par. Con estos commits los archivos de esta rebanada dejan de estar sin
seguimiento, que era la causa concreta del `unassessable` que el par reporto en su
`L10` punto 3.
