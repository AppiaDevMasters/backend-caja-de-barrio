# SDK de Open Payments para Python: evaluación

Fecha de la evaluación: 2026-10-16.
Fuentes verificadas: repositorio oficial, `pypi.org`, workflows de GitHub Actions y
ejecución local de sus pruebas.

## Veredicto

**No es una base confiable para producción y no debería ser una dependencia fijada
sin trabajo adicional.** Es un cliente en desarrollo activo temprano (versión `0.1.0`),
no publicado en PyPI, sin cobertura de pruebas real, con el CI en rojo y con un defecto
de empaquetado que rompe la importación en un entorno limpio.

Para el hackathon sirve como **referencia del protocolo**; como biblioteca de
confianza, todavía no.

## Aclaración de arquitectura (importante)

El SDK de Python es un **cliente** de Open Payments: sirve para quien *paga* e inicia
grants contra otro ASE. **No implementa el lado ASE**, es decir, no expone wallet
addresses, no recibe pagos, no emite webhooks ni actúa como IdP.

En Caja de Barrio hay dos roles distintos y conviene no mezclarlos:

| Rol | Quién lo cubre | Qué implica |
| --- | --- | --- |
| **ASE** (aloja cuentas y wallet addresses de las tiendas) | Rafiki (`rafiki/`) + nuestro backend | Admin API para cuentas y liquidez, webhooks, tipos de cambio, IdP |
| **Cliente** (inicia pagos hacia otros ASE) | El SDK de Python **o** un cliente propio | GNAP, firmas HTTP, incoming/quote/outgoing payment |

Si el backend de Caja de Barrio es el ASE, **el SDK de Python no participa**: lo que
hay que escribir es la integración con Rafiki (Admin API, webhooks, `/rates`, `/idp/`).
El SDK entra en escena sólo para pagos salientes hacia otros ASE.

## Evidencia

### Estado declarado por el propio proyecto

- El `README.md` oficial abre con: *"This project is still a work in progress"*.
- La documentación oficial de Open Payments
  ([openpayments.dev/sdk/before-you-begin](https://openpayments.dev/sdk/before-you-begin/))
  lista los SDK por estado: Go, Java, PHP, Rust y TypeScript están **Done**; Python y
  C# están **In Progress**.
- No está publicado en PyPI: `https://pypi.org/pypi/open-payments-python-sdk/json`
  devuelve **404**. El propio README indica que *"you will need to build from source"*.
  La instalación real es `poetry build` + `pip install dist/open_payments_sdk-0.1.0-py3-none-any.whl`.

### Actividad y mantenimiento

- 251 commits, 6 contribuidores, 5 estrellas, 11 issues abiertos.
- Último cambio de **código funcional**: 2025-10-02 (implementación inicial de GNAP y
  firmas HTTP). Después sólo hubo cambios de `README.md` (2026-01-19 y 2026-09-15).
  Es decir, alrededor de un año sin avance funcional.
- Los issues abiertos son trabajo base pendiente, no detalles: *"Create unittests"*,
  *"Add docs"*, *"Working example of using the python sdk"*, *"Functional one-time
  payments and tests"*, *"feat: add integration tests for incoming payments"*,
  *"Pydantic validation errors with API responses"*.

### Cobertura de pruebas

- 1592 líneas en `src/` contra 146 líneas en `tests/`.
- **Un solo test unitario**: `tests/unit/test_op_client.py::test_create_op_client`,
  que sólo verifica que el cliente se instancia.
- Cuatro tests de integración (`tests/integration/test_grants.py`,
  `tests/integration/test_wallet.py`) que golpean la red real
  (`wallet.interledger-test.dev`).
- El workflow de CI ejecuta **únicamente** `pytest tests/unit/`: los tests de
  integración nunca corren de forma automática.

### Integración continua

El último run del workflow `Test Python open payments sdk` (2026-09-15) terminó en
**failure**:

- `test (ubuntu-latest, 3.10 … 3.13)` → pasa. Las pruebas unitarias funcionan en
  Python 3.10 a 3.13 sobre Linux.
- `test (windows-latest, 3.10 … 3.13)` → **falla** en el paso *Install dependencies*.
- job `audit` (`pip-audit`) → **falla**, es decir hay dependencias con
  vulnerabilidades conocidas y el proyecto no tiene el pipeline en verde.

### Defecto de empaquetado verificado localmente

`src/open_payments_sdk/gnap_utils/security.py` hace `import http_sfv`, pero
`http_sfv` **no está declarado** en `pyproject.toml`; sólo aparece en `poetry.lock`
como dependencia transitiva de `http-message-signatures`.

Reproducción, en un entorno virtual limpio con las dependencias declaradas:

```bash
pip install "httpx>=0.28,<0.29" "pydantic>=2.10.6" "cryptography>=45" \
            "http-message-signatures>=0.6.1"
python -c "import open_payments_sdk"
# ModuleNotFoundError: No module named 'http_sfv'
```

La instalación funciona hoy por casualidad de resolución de dependencias
transitivas, no por contrato. Si esa transitiva desaparece, el paquete queda roto en
la importación. Con `http-sfv` instalado aparte, el único test unitario pasa.

### Ejemplo funcional: existe, pero fuera del repositorio oficial

El mantenedor mantiene un fork con un ejemplo peer-to-peer completo (script principal,
servidor de callback y `docker-compose.yml`) en la rama `working-example-python-sdk`
de `johngian/open-payments-python-sdk`, **sin mergear** al repositorio de
Interledger, mientras el issue *"Working example of using the python sdk"* sigue abierto.
Ese ejemplo es la mejor referencia práctica disponible, pero no es código soportado.

## Opciones

| Opción | Esfuerzo | Riesgo | Cuándo conviene |
| --- | --- | --- | --- |
| **A. Cliente propio** con `httpx` + `cryptography` + `http-message-signatures`, usando el SDK como referencia | Medio | Bajo: control total, sin dependencias frágiles | Cuando el cliente Open Payments es parte del producto |
| **B. Vendorizar el SDK** (clonar, `poetry build`, wheel al repo, commit fijado) | Bajo | Alto: hereda los huecos, el CI rojo y la dependencia no declarada | Como acelerador, con fecha de caducidad |
| **C. Usar un SDK con estado "Done"** (Go, Rust, PHP, Java, TypeScript) | Medio-alto | Bajo en el cliente, pero obliga a otro lenguaje o a un servicio intermedio | Si el equipo ya domina ese lenguaje |

## Recomendación

1. **No** agregar `open-payments-python-sdk` como dependencia instalable esperada:
   no existe en PyPI. Si se usa, vendorizar el wheel y fijar el commit exacto, y
   declarar `http-sfv` explícitamente en las dependencias del proyecto.
2. Tratar el SDK como **referencia del protocolo** (estructura de grants GNAP, RFC 9421,
   claves Ed25519) más que como biblioteca confiable.
3. Para el backend de Caja de Barrio, el trabajo inmediato no es el SDK: es la
   integración con Rafiki como ASE (Admin API, webhooks, `/rates`, `/idp/`). Ver
   `rafiki/README.md`.
4. Si el tiempo del hackathon alcanza, escribir el cliente con `httpx` +
   `cryptography`: el backend igual necesita la mitad de esa lógica (claves,
   firmas, verificación de webhooks), así que la inversión no se duplica.

## Cómo reproducir esta evaluación

```bash
# 1. El paquete no existe en PyPI
curl -s -o /dev/null -w '%{http_code}\n' \
  https://pypi.org/pypi/open-payments-python-sdk/json        # 404

# 2. Estado del CI
curl -s "https://api.github.com/repos/interledger/open-payments-python-sdk/actions/runs?per_page=5" \
  | grep -E '"conclusion"|"created_at"'

# 3. Cobertura real
git clone https://github.com/interledger/open-payments-python-sdk
cd open-payments-python-sdk
grep -c 'def test_' tests/unit/*.py                          # 1
grep -c 'def test_' tests/integration/*.py                   # 2 por archivo
grep -n 'pytest tests' .github/workflows/test.yml            # sólo tests/unit

# 4. Dependencia no declarada
grep -rn 'import http_sfv' src/
grep -n 'sfv' pyproject.toml                                 # sin resultados
```
