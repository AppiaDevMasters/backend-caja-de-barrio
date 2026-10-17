-- El panel de control: quien entra, que hizo y por que una persona quedo en revision.
--
-- Las claves del panel no viven en el repo: el backend las lee de deploy/llaves.env
-- al arrancar y guarda solo su hash bcrypt.

create table if not exists usuarios_panel (
	username   text primary key,
	nombre     text not null,
	rol        text not null check (rol in ('superadmin', 'cumplimiento', 'soporte')),
	clave_hash text not null,
	creado_en  timestamptz not null default now()
);

-- Toda escritura del panel deja su rastro, con el motivo que escribio la persona.
create table if not exists auditoria (
	id         text primary key,
	ts         timestamptz not null default now(),
	actor      text not null,
	accion     text not null,
	entidad    text not null,
	entidad_id text not null,
	motivo     text,
	detalle    jsonb not null default '{}'::jsonb
);

create index if not exists auditoria_entidad_idx on auditoria (entidad, entidad_id, ts desc);

-- Por que el registro no se aprobo solo (por ejemplo, Verifik no confirmo el documento).
alter table personas add column if not exists motivo_revision text;
