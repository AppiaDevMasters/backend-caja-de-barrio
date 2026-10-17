#!/usr/bin/env bash
#
# Instala el stack de Caja de Barrio (Rafiki + backend Go) en un VPS Ubuntu limpio,
# con HTTPS de Let's Encrypt sobre nombres de sslip.io (no hace falta comprar dominio).
#
# Uso, en el VPS, desde la raíz del repositorio clonado:
#
#   sudo ./deploy/instalar.sh
#
# Variables opcionales:
#   DOMINIO      Dominio base. Por defecto <ip-con-guiones>.sslip.io (ej. 203-0-113-5.sslip.io).
#   IP_PUBLICA   Si no se puede detectar la IP pública sola.
#
# Es idempotente: se puede volver a correr después de un `git pull` para reconstruir.
set -euo pipefail

raiz="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$raiz"
env_rafiki="$raiz/rafiki/.env"
credenciales="$raiz/deploy/.credenciales-admin"
compose=(docker compose -f rafiki/docker-compose.yml -f deploy/compose.vps.yml)

paso() { printf '\n==> %s\n' "$*"; }
env_val() { sed -n "s/^$1=//p" "$env_rafiki" | head -n 1; }

[ "$(id -u)" -eq 0 ] || { echo "Corre con sudo: sudo ./deploy/instalar.sh" >&2; exit 1; }

paso "Docker"
if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sh
fi
docker compose version

paso "Firewall: solo SSH, 80 y 443"
if command -v ufw >/dev/null; then
  ufw allow OpenSSH >/dev/null
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  ufw --force enable >/dev/null
  ufw status | sed -n '1,10p'
else
  echo "ufw no está instalado: abre solo 22, 80 y 443 en el firewall del proveedor."
fi

paso "Dominio"
ip="${IP_PUBLICA:-$(curl -fsS https://api.ipify.org)}"
# Orden: DOMINIO del entorno, el que ya quedó guardado en rafiki/.env (para que una
# reinstalación no lo cambie sin querer) y, si no hay ninguno, <ip-con-guiones>.sslip.io.
dominio_guardado="$( [ -f "$env_rafiki" ] && env_val DOMINIO || true )"
dominio="${DOMINIO:-${dominio_guardado:-$(echo "$ip" | tr '.' '-').sslip.io}}"
if [ -n "$dominio_guardado" ] && [ "$dominio" != "$dominio_guardado" ]; then
  echo "El dominio cambia de $dominio_guardado a $dominio: Rafiki guarda el dominio en su base," >&2
  echo "así que hay que empezar de cero (ver 'Cambiar de dominio' en deploy/README.md)." >&2
  exit 1
fi
echo "IP pública: $ip"
echo "Dominio:    $dominio"

paso "Configuración y claves de Rafiki"
if [ ! -f "$env_rafiki" ]; then
  (cd rafiki && RAFIKI_PUBLIC_HOST="$dominio" ./scripts/init.sh)
else
  echo "rafiki/.env ya existe; se conserva (mismos secretos y datos)."
fi

# Usuario y clave de la UI de administración de Rafiki (detrás de Caddy).
if [ ! -f "$credenciales" ]; then
  clave_admin="$(openssl rand -base64 18 | tr -d '/+=')"
  umask 077
  printf 'usuario=admin\nclave=%s\n' "$clave_admin" >"$credenciales"
fi
clave_admin="$(sed -n 's/^clave=//p' "$credenciales")"
hash_admin="$(docker run --rm caddy:2.8-alpine caddy hash-password --plaintext "$clave_admin")"

# Secretos del producto: se generan una vez y se conservan entre corridas (la base ya
# tiene esa clave y los MCP ya tienen ese token).
db_password="$(env_val CAJA_DB_PASSWORD)"; db_password="${db_password:-$(openssl rand -hex 24)}"
servicio_token="$(env_val SERVICIO_TOKEN)"; servicio_token="${servicio_token:-$(openssl rand -hex 32)}"
verifik_token="${VERIFIK_TOKEN:-$(env_val VERIFIK_TOKEN)}"

# Bloque del VPS al final de rafiki/.env, reescrito en cada corrida. El hash va entre
# comillas simples para que Compose no interprete sus `$`.
sed -i '/^# --- VPS (deploy\/instalar.sh) ---$/,$d' "$env_rafiki"
cat >>"$env_rafiki" <<EOF
# --- VPS (deploy/instalar.sh) ---
DOMINIO=$dominio
ADMIN_UI_USUARIO=admin
ADMIN_UI_HASH='$hash_admin'
CAJA_DB_PASSWORD=$db_password
SERVICIO_TOKEN=$servicio_token
VERIFIK_TOKEN=$verifik_token
EOF

paso "Directorio de la clave del cliente Open Payments"
mkdir -p keys
chown 1000:1000 keys

paso "Levantando el stack"
"${compose[@]}" up -d --build

paso "Esperando a rafiki-backend"
for _ in $(seq 1 60); do
  estado="$(docker inspect -f '{{.State.Health.Status}}' rafiki-backend 2>/dev/null || true)"
  [ "$estado" = healthy ] && break
  sleep 5
done
[ "$estado" = healthy ] || { echo "rafiki-backend no quedó sano; revisa: docker logs rafiki-backend" >&2; exit 1; }

paso "Seed del ASE (activo COP, liquidez, wallet addresses y clave del cliente)"
# Corre dentro de la red del stack, como uid 1000 para que la clave quede legible por
# el contenedor del backend. Las wallet addresses se publican en https://op.<dominio>.
docker run --rm \
  --network rafiki-caja-barrio_rafiki \
  --user 1000:1000 \
  -e HOME=/tmp -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go \
  -e RAFIKI_ADMIN_URL=http://rafiki-backend:3001/graphql \
  -e RAFIKI_OPEN_PAYMENTS_URL="https://op.$dominio" \
  -e RAFIKI_TENANT_ID="$(env_val OPERATOR_TENANT_ID)" \
  -e RAFIKI_ADMIN_API_SECRET="$(env_val ADMIN_API_SECRET)" \
  -e RAFIKI_SIGNATURE_SECRET="$(env_val SIGNATURE_SECRET)" \
  -e RAFIKI_IDENTITY_SERVER_SECRET="$(env_val IDENTITY_SERVER_SECRET)" \
  -v "$raiz:/src" -w /src \
  golang:1.22-alpine go run ./cmd/seed

paso "Reiniciando el backend para que cargue la clave del cliente"
"${compose[@]}" restart backend
for _ in $(seq 1 30); do
  estado="$(docker inspect -f '{{.State.Health.Status}}' caja-de-barrio-backend 2>/dev/null || true)"
  [ "$estado" = healthy ] && break
  sleep 3
done
[ "$estado" = healthy ] || { echo "el backend no quedó sano; revisa: docker logs caja-de-barrio-backend" >&2; exit 1; }

paso "Datos del demo (tiendas, clientas y efectivo de hoy y mañana)"
# Corre después de que el backend aplicó sus migraciones al arrancar.
docker exec -i caja-de-barrio-db psql -q -v ON_ERROR_STOP=1 -v base="https://op.$dominio"   -U caja -d caja_de_barrio <deploy/semilla-demo.sql

paso "Verificación"
sleep 5
for url in "https://backend.$dominio/health" "https://backend.$dominio/rates?base=USD"; do
  printf '%-55s ' "$url"
  curl -fsS --retry 10 --retry-delay 3 --retry-all-errors "$url" || echo "FALLÓ"
  echo
done
printf '%-55s ' "https://backend.$dominio/internal (con token)"
curl -fsS -H "X-Servicio-Token: $servicio_token" "https://backend.$dominio/internal/identidad/personas?whatsapp=573000000001" || echo "FALLÓ"
echo
printf '%-55s ' "https://op.$dominio/app-cajadebarrio"
curl -fsS --retry 5 --retry-delay 3 -H 'Accept: application/json' "https://op.$dominio/app-cajadebarrio" | head -c 300 || echo "FALLÓ"
echo

cat <<EOF

Listo. URLs públicas:

  Open Payments (wallet addresses)  https://op.$dominio/app-cajadebarrio
  Servidor de autorización (GNAP)   https://auth.$dominio
  Backend Go (IdP y API)            https://backend.$dominio
  UI de Rafiki                      https://admin.$dominio   (usuario y clave en deploy/.credenciales-admin)

Wallet addresses del demo:
  https://op.$dominio/tienda-cajadebarrio
  https://op.$dominio/laura-cajadebarrio
  https://op.$dominio/app-cajadebarrio

El SERVICIO_TOKEN de los MCP está en rafiki/.env (no se imprime aquí).

Para actualizar después de un git pull:  sudo ./deploy/instalar.sh
EOF
