package iam

import "testing"

// Одно устройство — одна роль.
//
// Если в браузере остаются обе cookie, сервер отдаёт приоритет учителю, а
// ручки ученика начинают отвечать 404. Экран при этом здоровается с
// учеником — интерфейс и сервер расходятся, и человек видит «ты не в
// классе» сразу после того, как в него вошёл.
//
// Здесь закрепляется само правило приоритета: пока оно такое, вход в одной
// роли ОБЯЗАН убирать cookie другой (см. API.ClearSessionCookie и
// clearCookie в обработчике входа).

func TestActorKindsAreExclusive(t *testing.T) {
	teacher := Actor{Kind: "user"}
	student := Actor{Kind: "student"}

	if !teacher.IsUser() || teacher.IsStudent() {
		t.Error("актор-учитель не должен считаться учеником")
	}
	if !student.IsStudent() || student.IsUser() {
		t.Error("актор-ученик не должен считаться учителем")
	}
}

// У учителя нет StudentID, у ученика — UserID. Ручка ученика, вызванная с
// актором учителя, ушла бы искать классы нулевого идентификатора и вернула
// бы пустоту вместо отказа. Поэтому такие ручки проверяют роль явно.
func TestTeacherHasNoStudentIdentity(t *testing.T) {
	teacher := Actor{Kind: "user"}
	if teacher.StudentID.String() != "00000000-0000-0000-0000-000000000000" {
		t.Error("у актора-учителя не должно быть идентификатора ученика")
	}
}
