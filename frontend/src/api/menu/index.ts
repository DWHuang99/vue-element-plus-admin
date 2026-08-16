import request from '@/axios'

export const getMenuListApi = () => {
  return request.get({ url: '/api/v1/menus/tree' })
}

export const saveMenuApi = (data: any) => {
  return data.id
    ? request.put({ url: `/api/v1/menus/${data.id}`, data })
    : request.post({ url: '/api/v1/menus', data })
}

export const deleteMenuApi = (id: string | number) => {
  return request.delete({ url: `/api/v1/menus/${id}` })
}
