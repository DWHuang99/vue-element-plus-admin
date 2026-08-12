package security

import "golang.org/x/crypto/bcrypt"

// Hash 对明文密码进行哈希
func Hash(plainPassword string) (string, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword(
		[]byte(plainPassword),
		bcrypt.DefaultCost,
	)
	if err != nil {
		return "", err
	}

	return string(hashedPassword), nil
}

// Verify 检查明文密码是否与哈希匹配
func Verify(plainPassword, hashedPassword string) bool {
	err := bcrypt.CompareHashAndPassword(
		[]byte(hashedPassword),
		[]byte(plainPassword),
	)

	return err == nil
}
