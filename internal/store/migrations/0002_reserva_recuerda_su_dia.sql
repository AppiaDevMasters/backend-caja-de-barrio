-- La reserva recuerda de que dia es el efectivo que tomo.
--
-- El dia no se puede recalcular despues sin suponer una zona horaria: el dia que
-- nombra el llamador (el de la demo) y el dia UTC no son el mismo dia por la tarde,
-- asi que una liberacion que recalculara el dia buscaria el efectivo de otro dia y
-- **nunca lo devolveria**. La columna guarda el dia que se uso, una sola vez.
--
-- LAS FILAS QUE YA EXISTIAN NO SE ADIVINAN. El dia de su efectivo es el que nombro
-- el llamador cuando se crearon, y ese dia no esta en la fila: deducirlo del
-- timestamp con la zona horaria de la sesion da un dia distinto por la tarde (se
-- comprobo: una reserva de las 20:00 de Bogota queda con el dia UTC siguiente), y
-- entonces la liberacion buscaria el efectivo de otro dia y **dejaria la plata de la
-- tienda inmovilizada en silencio**. Asi que esta migracion **se niega a adivinar**:
-- si hay filas que convertir, se detiene y el dia lo decide una persona.

alter table reservas add column if not exists fecha_efectivo date;

do $$
begin
	if exists (select 1 from reservas) then
		raise exception 'hay reservas sin fecha_efectivo y el dia no se puede deducir del timestamp sin arriesgar la zona horaria: decidi el dia a mano antes de aplicar esta migracion';
	end if;
end $$;

alter table reservas alter column fecha_efectivo set not null;
