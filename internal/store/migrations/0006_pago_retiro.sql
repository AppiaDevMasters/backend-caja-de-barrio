-- El pago del retiro con Open Payments (contrato 8.2 a 8.8).
--
-- cupos guarda lo que el callback del grant interactivo necesita para verificar el
-- hash (los dos nonces y el servidor de autorizacion) y el token ya cifrado.
-- retiros guarda la cotizacion vigente y el resultado del pago, que es lo que hace
-- idempotente un segundo POST /pagos de la misma reserva.

alter table cupos add column if not exists server_nonce text;
alter table cupos add column if not exists auth_server text;
alter table cupos add column if not exists token_vence_en timestamptz;
alter table cupos add column if not exists creado_en timestamptz not null default now();

alter table retiros add column if not exists debito numeric(20, 8);
alter table retiros add column if not exists moneda text;
alter table retiros add column if not exists quote_vence_en timestamptz;
alter table retiros add column if not exists resultado jsonb;
alter table retiros add column if not exists liquidez_en timestamptz;

-- Se busca la reserva por su cobro (contrato 8.5 y 8.8).
create index if not exists retiros_incoming_idx on retiros (incoming_payment_url);
