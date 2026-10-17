-- Billetera virtual de la demo: cada movimiento suma o resta a una wallet address.
--
-- El saldo es la suma de sus movimientos. La referencia hace idempotente cada
-- movimiento: la carga inicial de un registro usa la sesion de Didit que lo aprobo,
-- asi que consultar la decision dos veces no carga dos veces.

create table if not exists movimientos (
	id             text primary key,
	wallet_address text not null,
	tipo           text not null,
	monto          bigint not null,
	referencia     text not null,
	creado_en      timestamptz not null default now(),
	unique (wallet_address, tipo, referencia)
);

create index if not exists movimientos_wallet_idx on movimientos (wallet_address, creado_en desc);
