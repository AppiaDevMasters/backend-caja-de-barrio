-- El enlace de aprobacion del cupo pendiente: si el agente vuelve a pedir el cupo
-- mientras la persona todavia no lo aprueba, se devuelve el mismo enlace en vez de
-- reemplazar el grant (el enlace anterior quedaria muerto: "No existe ese cupo").
alter table cupos add column if not exists redirect_url text;
