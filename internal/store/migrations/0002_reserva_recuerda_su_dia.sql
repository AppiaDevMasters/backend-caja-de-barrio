-- La reserva recuerda de que dia es el efectivo que tomo.
--
-- El dia no se puede recalcular despues sin suponer una zona horaria: el dia que
-- nombra el llamador (el de la demo) y el dia UTC no son el mismo dia por la tarde,
-- asi que una liberacion que recalculara el dia buscaria el efectivo de otro dia y
-- **nunca lo devolveria**. La columna guarda el dia que se uso, una sola vez.
--
-- La carga inicial de las filas que ya existian usa el dia de la base; es lo mejor
-- que se puede hacer con datos de desarrollo, y las reservas nuevas quedan correctas
-- por construccion.

alter table reservas add column if not exists fecha_efectivo date;

update reservas set fecha_efectivo = creada_en::date where fecha_efectivo is null;

alter table reservas alter column fecha_efectivo set not null;
