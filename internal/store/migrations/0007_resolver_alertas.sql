-- El panel resuelve alertas: queda que se hizo (la accion) y cuando.
alter table alertas add column if not exists resolucion text;
alter table alertas add column if not exists resuelta_en timestamptz;
