-- Bases de datos de Rafiki para el ASE "Caja de Barrio".
-- Se ejecuta una sola vez, en la inicializacion del volumen de Postgres.
--
--   rafiki_backend -> recursos de Open Payments, cuentas y contabilidad (Postgres)
--   rafiki_auth    -> grants GNAP, access tokens y sesiones de interaccion
--
-- El owner es POSTGRES_USER (el superusuario de la imagen), que es el mismo
-- usuario con el que se conectan rafiki-backend y rafiki-auth.

CREATE DATABASE rafiki_backend;
CREATE DATABASE rafiki_auth;
