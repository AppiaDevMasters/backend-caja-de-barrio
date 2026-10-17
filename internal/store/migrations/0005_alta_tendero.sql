-- Alta del tendero desde el panel (panel-de-control-v3.md, 3.3).
--
-- El panel crea la tienda en pendiente_verificacion; el tendero escribe al agente,
-- pasa por Didit y Verifik, y la tienda queda activa (o en revision_manual para que
-- el panel decida). El mapa solo muestra tiendas activas, asi que una tienda
-- pendiente nunca recibe retiros.

alter table tiendas drop constraint if exists tiendas_estado_check;
alter table tiendas add constraint tiendas_estado_check
	check (estado in ('activa', 'inactiva', 'pendiente_verificacion', 'revision_manual'));

alter table tiendas add column if not exists cedula_tendero text;
alter table tiendas add column if not exists motivo_revision text;
alter table tiendas add column if not exists creada_en timestamptz not null default now();

-- Una sesion de Didit es de una persona o de una tienda (la de su tendero).
alter table sesiones_verificacion alter column persona_id drop not null;
alter table sesiones_verificacion add column if not exists tienda_id text references tiendas (id) on delete restrict;
alter table sesiones_verificacion drop constraint if exists sesiones_verificacion_motivo_check;
alter table sesiones_verificacion add constraint sesiones_verificacion_motivo_check
	check (motivo in ('alta', 'retiro', 'alta_tendero'));
alter table sesiones_verificacion drop constraint if exists sesiones_verificacion_duenio_check;
alter table sesiones_verificacion add constraint sesiones_verificacion_duenio_check
	check (persona_id is not null or tienda_id is not null);

create index if not exists sesiones_verificacion_tienda_idx on sesiones_verificacion (tienda_id);
