-- Esquema de la capa de datos de Caja de Barrio: las once tablas del contrato
-- (contexto-caja-de-barrio/contrato-backend.md, seccion 9) mas la extension que
-- necesitan.
--
-- Lo aplica el servicio PostGIS del stack la primera vez que arranca con un
-- volumen vacio, y no hay herramienta de migraciones a proposito: durante la demo
-- cambiar el esquema es recrear el volumen, y un versionador solo agregaria una
-- pieza que nadie mira. Los archivos se numeran para que el orden sea evidente.
--
-- Nombres en espanol porque son los del contrato y los consumen los MCP; los
-- comentarios en ingles porque son codigo.

create extension if not exists postgis;

-- Una persona del barrio: el cliente que retira y, mas adelante, el tendero.
--
-- cedula holds the full number, because Verifik validates it and the duplicate
-- check needs it; the contract's rule is about what is SHOWN, so the API masks it
-- on the way out and the number never leaves the backend unmasked.
create table personas (
	id                      text primary key,
	whatsapp                text not null unique,
	cedula                  text not null,
	nombre                  text not null,
	wallet_address          text not null,
	nivel                   smallint not null default 1 check (nivel between 1 and 2),
	estado_registro         text not null default 'pendiente'
		check (estado_registro in ('pendiente', 'verificando', 'revision_manual', 'aprobada', 'rechazada', 'bloqueada')),
	verificacion_session_id text,
	-- The thirty minutes of Didit verification the contract requires before a
	-- reservation can be made.
	verificado_hasta        timestamptz,
	creada_en               timestamptz not null default now()
);

-- Una tienda del barrio, con su punto geografico y su tendero.
--
-- The withdrawal ceilings by store level (basico 300.000, comercio 1.000.000,
-- aliado 3.000.000) live in the code and not here: the contract fixes them as
-- demo values and no column of its section 9 carries them.
create table tiendas (
	id               text primary key,
	nombre           text not null,
	direccion        text not null,
	ubicacion        geography(Point, 4326) not null,
	nivel            text not null check (nivel in ('basico', 'comercio', 'aliado')),
	estado           text not null default 'activa' check (estado in ('activa', 'inactiva')),
	wallet_address   text not null,
	whatsapp_tendero text not null,
	nombre_tendero   text not null,
	reputacion       numeric(3, 2) not null default 0 check (reputacion between 0 and 5)
);

-- The nearest-store search of the contract (ST_DWithin plus the KNN operator)
-- walks this index.
create index tiendas_ubicacion_gist on tiendas using gist (ubicacion);
create index tiendas_whatsapp_tendero_idx on tiendas (whatsapp_tendero);

-- Cash a store publishes for one day, and how much of it is already reserved.
-- The check is the rule: a store cannot reserve more than it published.
create table efectivo_disponible (
	tienda_id     text not null references tiendas (id) on delete restrict,
	fecha         date not null,
	publicado_cop bigint not null check (publicado_cop >= 0),
	reservado_cop bigint not null default 0 check (reservado_cop >= 0),
	primary key (tienda_id, fecha),
	check (reservado_cop <= publicado_cop)
);

-- The reservation of cash: the heart of the flow.
--
-- Money in COP is a whole number of pesos and lives in bigint. The code is the
-- four digits only the client sees, and the format check keeps a wrong one from
-- being stored at all.
create table reservas (
	id                      text primary key,
	tienda_id               text not null references tiendas (id) on delete restrict,
	persona_id              text not null references personas (id) on delete restrict,
	monto_cop               bigint not null check (monto_cop > 0),
	comision_tendero_cop    bigint not null check (comision_tendero_cop >= 0),
	comision_plataforma_cop bigint not null check (comision_plataforma_cop >= 0),
	codigo                  text not null check (codigo ~ '^[0-9]{4}$'),
	estado                  text not null default 'reservada'
		check (estado in ('reservada', 'pagada', 'entregada', 'vencida', 'cancelada', 'en_disputa')),
	riesgo_ok               boolean not null default false,
	face_match_ok           boolean not null default false,
	verificacion_session_id text,
	motivo_cancelacion      text,
	aprobacion_en           timestamptz,
	pagada_en               timestamptz,
	entrega_tendero_en      timestamptz,
	recibido_cliente_en     timestamptz,
	entregado_en            timestamptz,
	creada_en               timestamptz not null default now(),
	vence_en                timestamptz not null
);

create index reservas_tienda_estado_idx on reservas (tienda_id, estado);
-- The anti-pitufeo rule counts one person's paid or delivered reservations in the
-- last sixty minutes, so this is the index it reads.
create index reservas_persona_estado_pagada_idx on reservas (persona_id, estado, pagada_en);

-- The Open Payments handles of one withdrawal. One row per reservation, filled as
-- the flow advances; it is what an operator needs to trace money at the providers.
create table retiros (
	reserva_id             text primary key references reservas (id) on delete restrict,
	incoming_payment_url   text,
	cobro_plataforma_url   text,
	quote_url              text,
	quote_plataforma_url   text,
	outgoing_payment_id    text,
	outgoing_plataforma_id text,
	actualizado_en         timestamptz not null default now()
);

-- One Didit session, for the registration or for the face match at the counter.
-- Only the reference is kept: the contract forbids storing selfies or document
-- photos.
create table sesiones_verificacion (
	id               text primary key,
	persona_id       text not null references personas (id) on delete restrict,
	motivo           text not null check (motivo in ('alta', 'retiro')),
	didit_session_id text,
	aprobada         boolean,
	-- No precision on purpose: the provider documents one score per feature and
	-- their scale is not fixed anywhere.
	score            numeric,
	creada_en        timestamptz not null default now(),
	resuelta_en      timestamptz
);

create index sesiones_verificacion_persona_idx on sesiones_verificacion (persona_id, motivo);

-- The interactive grant that caps what one person can withdraw (contract 8.2):
-- one per person, asked once.
--
-- access_token holds CIPHERTEXT: the value is encrypted by the application before
-- it lands here, and the scheme is a decision of the payments route, not of the
-- schema. limite and usado are numeric because the cupo lives in the client's own
-- currency and a dollar amount has decimals.
create table cupos (
	persona_id       text primary key references personas (id) on delete restrict,
	grant_id         text not null unique,
	wallet_address   text not null,
	limite           numeric(20, 8) not null check (limite > 0),
	moneda           text not null,
	intervalo        text not null,
	usado            numeric(20, 8) not null default 0 check (usado >= 0),
	access_token     text,
	token_manage_url text,
	continue_uri     text,
	continue_token   text,
	nonce            text,
	aprobado_en      timestamptz,
	-- The rule of the contract: what was used plus what is being paid never
	-- exceeds the ceiling the grant imposed.
	check (usado <= limite)
);

-- A rule fired. An alert is always about a person or a store, and the check says
-- so instead of leaving a row that points at nobody.
create table alertas (
	id         text primary key,
	persona_id text references personas (id) on delete restrict,
	tienda_id  text references tiendas (id) on delete restrict,
	regla      text not null check (regla in ('pitufeo', 'verificacion_facial_fallida', 'entrega_no_recibida')),
	detalle    text not null,
	estado     text not null default 'abierta' check (estado in ('abierta', 'resuelta')),
	creada_en  timestamptz not null default now(),
	check (persona_id is not null or tienda_id is not null)
);

-- The three tables the contract marks as insert-only: they are the trail of what a
-- person accepted, approved and confirmed. The trigger makes the rule impossible to
-- break by accident, which a comment cannot do.
create table aceptaciones_politicas (
	id            text primary key,
	whatsapp      text not null,
	version       text not null,
	texto_usuario text not null,
	aceptada_en   timestamptz not null default now()
);

create table aprobaciones_pago (
	id            text primary key,
	reserva_id    text not null references reservas (id) on delete restrict,
	persona_id    text not null references personas (id) on delete restrict,
	quote_url     text not null,
	-- The debit is in the client's currency, and a dollar quote has decimals
	-- (contract 8.5 speaks of 51.25), so this cannot be a whole number.
	debito        numeric(20, 8) not null check (debito > 0),
	moneda        text not null,
	texto_usuario text not null,
	canal         text not null,
	aprobada_en   timestamptz not null default now()
);

create index aprobaciones_pago_reserva_idx on aprobaciones_pago (reserva_id);

create table confirmaciones_entrega (
	id            text primary key,
	reserva_id    text not null references reservas (id) on delete restrict,
	parte         text not null check (parte in ('tendero', 'cliente')),
	-- The storekeeper's confirmation carries only the code in contract 7.6, so its
	-- row is always a received one; the client says it explicitly.
	recibido      boolean not null default true,
	texto_usuario text,
	confirmada_en timestamptz not null default now()
);

create index confirmaciones_entrega_reserva_idx on confirmaciones_entrega (reserva_id, parte);

create or replace function prohibir_cambio() returns trigger as $$
begin
	raise exception 'la tabla % es de solo insercion', tg_table_name;
end;
$$ language plpgsql;

create trigger aceptaciones_politicas_solo_insercion
	before update or delete on aceptaciones_politicas
	for each row execute function prohibir_cambio();

create trigger aprobaciones_pago_solo_insercion
	before update or delete on aprobaciones_pago
	for each row execute function prohibir_cambio();

create trigger confirmaciones_entrega_solo_insercion
	before update or delete on confirmaciones_entrega
	for each row execute function prohibir_cambio();
