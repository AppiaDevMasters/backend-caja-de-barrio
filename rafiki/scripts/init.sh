#!/usr/bin/env bash
#
# Genera la configuracion local de Rafiki: rafiki/.env y rafiki/config/keys/.
#
#   ./scripts/init.sh            # crea .env y claves si no existen
#   ./scripts/init.sh --force    # regenera .env y claves (borra el estado previo)
#
# Ningun secreto se versiona: .env y config/keys/ estan en .gitignore.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="$here/.env"
keys_dir="$here/config/keys"
force=0

# Host con el que se anuncian las URLs publicas. Tiene que ser alcanzable por el
# navegador del usuario y por los clientes Open Payments, asi que `localhost` solo
# sirve si todo corre en el propio host. Para acceso desde otros dispositivos:
#
#   RAFIKI_PUBLIC_HOST=100.76.141.47 ./scripts/init.sh --force
#
public_host="${RAFIKI_PUBLIC_HOST:-localhost}"

for arg in "$@"; do
  case "$arg" in
    --force) force=1 ;;
    -h | --help)
      sed -n '2,9p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "Argumento desconocido: $arg" >&2
      exit 2
      ;;
  esac
done

command -v openssl >/dev/null || {
  echo "Falta openssl." >&2
  exit 1
}

if [ -f "$env_file" ] && [ "$force" -eq 0 ]; then
  echo "Ya existe $env_file (usa --force para regenerarlo)."
  exit 0
fi

# --- generadores ------------------------------------------------------------
# base64: apto para secretos HMAC y claves simetricas.
# hex: apto para incrustar en URLs de conexion (postgresql://user:pass@host).
b64_32() { openssl rand -base64 32 | tr -d '\n'; }
hex_32() { openssl rand -hex 32; }
uuid4() {
  if [ -r /proc/sys/kernel/random/uuid ]; then
    cat /proc/sys/kernel/random/uuid
  else
    python3 -c 'import uuid; print(uuid.uuid4())'
  fi
}

postgres_password="$(hex_32)"
admin_api_secret="$(b64_32)"
signature_secret="$(b64_32)"
stream_secret="$(b64_32)"
db_encryption_secret="$(b64_32)"
identity_server_secret="$(b64_32)"
cookie_key="$(hex_32)"
key_id="$(uuid4)"
operator_tenant_id="$(uuid4)"

umask 022
cat >"$env_file" <<EOF
# Generado por rafiki/scripts/init.sh el $(date -u +%Y-%m-%dT%H:%M:%SZ).
# NO versionar. Regenerar con: ./scripts/init.sh --force

# --- Rafiki ----------------------------------------------------------------
# Version de las imagenes ghcr.io/interledger/rafiki-*.
RAFIKI_VERSION=v2.4.5-de

# --- Postgres --------------------------------------------------------------
POSTGRES_USER=rafiki
POSTGRES_PASSWORD=$postgres_password
BACKEND_DB=rafiki_backend
AUTH_DB=rafiki_auth
POSTGRES_PORT=5432

# --- ASE -------------------------------------------------------------------
INSTANCE_NAME=Caja de Barrio
OPERATOR_TENANT_ID=$operator_tenant_id
# kid con el que se registra la clave publica del ASE en el auth server.
KEY_ID=$key_id

# --- Secretos --------------------------------------------------------------
ADMIN_API_SECRET=$admin_api_secret
# Firma los webhooks que Rafiki envia a nuestro backend y las llamadas a la Admin API.
SIGNATURE_SECRET=$signature_secret
STREAM_SECRET=$stream_secret
DB_ENCRYPTION_SECRET=$db_encryption_secret
IDENTITY_SERVER_SECRET=$identity_server_secret
COOKIE_KEY=$cookie_key

# --- URLs publicas del entorno local --------------------------------------
# Base de las wallet addresses que expone el servidor de recursos.
OPEN_PAYMENTS_URL=http://$public_host:3000
AUTH_SERVER_URL=http://$public_host:3006
# IdP que abre el navegador durante un grant interactivo (lo sirve nuestro backend).
# OJO: el auth server lo copia a la columna idpConsentUrl en el primer arranque;
# si cambias este valor despues, hay que actualizar esa columna (ver README).
IDENTITY_SERVER_URL=http://$public_host:8000/idp/
# Endpoints de nuestro backend vistos desde los contenedores.
WEBHOOK_URL=http://host.docker.internal:8000/webhooks
EXCHANGE_RATES_URL=http://host.docker.internal:8000/rates

# --- Puertos publicados en el host ----------------------------------------
OPEN_PAYMENTS_PORT=3000
ADMIN_API_PORT=3001
CONNECTOR_PORT=3002
AUTH_ADMIN_PORT=3003
AUTH_PORT=3006
INTROSPECTION_PORT=3007
INTERACTION_PORT=3009
ADMIN_UI_PORT=3010
AUTH_SERVICE_API_PORT=3011

LOG_LEVEL=debug

# --- Acceso desde otros dispositivos --------------------------------------
# El stack corre en un host, no en tu equipo: "localhost" solo sirve para clientes
# que corren en ese mismo host (contenedores, curl local).
# Admin API (GraphQL):       http://$public_host:3001/graphql  (firma HMAC, ver README)
# Servidor Open Payments:    http://$public_host:3000
# Credenciales: ADMIN_API_SECRET + OPERATOR_TENANT_ID (arriba), tanto para la
# Admin API como para la UI de administracion.
#
# UI (http://$public_host:3010): su cookie de sesion __session esta fijada como
# Secure en la imagen del frontend, asi que el navegador la descarta sobre http y
# la UI queda en 401. Solo funciona en origen seguro: tunel SSH al host
# (http://localhost:3010) o Tailscale Serve con HTTPS. Ver README.
EOF

# --- clave Ed25519 del ASE --------------------------------------------------
mkdir -p "$keys_dir"

priv="$keys_dir/private-key.pem"
pub="$keys_dir/public-key.raw"
jwks="$keys_dir/jwks.json"

if [ ! -f "$priv" ] || [ "$force" -eq 1 ]; then
  openssl genpkey -algorithm ed25519 -out "$priv"
  openssl pkey -in "$priv" -pubout -outform DER | tail -c 32 >"$pub"
fi

# El contenedor corre como el usuario `rafiki` (uid 1000); el archivo se monta
# en solo lectura y debe ser legible dentro del contenedor.
chmod 644 "$priv" "$pub"

# x es la clave publica cruda en base64url (RFC 7518) y debe coincidir con KEY_ID.
x="$(openssl base64 -A -in "$pub" | tr '+/' '-_' | tr -d '=')"
cat >"$jwks" <<EOF
{
  "keys": [
    {
      "kty": "OKP",
      "crv": "Ed25519",
      "x": "$x",
      "kid": "$key_id",
      "alg": "EdDSA",
      "use": "sig"
    }
  ]
}
EOF
chmod 644 "$jwks"

cat <<EOF

Listo.

  .env                    $env_file
  clave privada del ASE   $priv
  JWKS publico (kid=$key_id)
                          $jwks
  host de las URLs        $public_host

La clave publica de $jwks es la que nuestro backend debe registrar
en el auth server. El siguiente paso:

  docker compose up -d
EOF
